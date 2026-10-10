import { TransportTelemetry, TransportTelemetrySender, type TransportSample } from "./transport-telemetry";
import { AudioRuntimeTelemetry } from "./audio-runtime-telemetry";
import { mediaFailure, safeMediaSessionEvent, type MediaRecoveryContext, type MediaSessionEvent } from "../frontend/src/media-lease";
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

import { playRingback, ringbackPattern } from "./ringback";

const SAMPLE_RATE = 24_000;
const JITTER_TARGET_MS = 60;
const MAX_RECONNECT_MS = 30_000;

async function loadAudioWorklet(context: AudioContext, url: string): Promise<void> {
  try { await context.audioWorklet.addModule(url); }
  catch (cause) {
    throw new Error("Telephony audio processor could not load. Check the site's Content Security Policy and reload after any Telephony update.", { cause });
  }
}

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
export class PreviewResampler {
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

/** Server-pushed call progress over the media socket (type "call.status"). */
export interface SoftphoneCallStatus {
  call_id: string;
  status: string;
  direction?: string;
  answered_at?: string;
  ended_at?: string;
  answered_by?: string;
  termination?: { reason?: string; cause?: string; code?: string; initiator?: string };
  hold_state?: "active" | "starting" | "held" | "stopping" | "unknown" | "ended";
  recording_state?: "off" | "active" | "pause_requested" | "paused" | "resume_requested" | "unknown" | "ended";
  control_error?: string;
}

export interface SoftphoneAudioHealth {
  state: "healthy" | "audio_degraded";
  reason?: "audio_degraded";
  stages: Record<string, {state: "healthy" | "audio_degraded" | "inactive"; reason?: string; signal?: string; last_bad_at?: string}>;
}
export interface SoftphoneCallbacks {
  onAudioHealth?: (health: SoftphoneAudioHealth) => void;
  refreshMediaURL?: (recovery?: MediaRecoveryContext) => Promise<string>;
  onSessionEvent?: (event: MediaSessionEvent) => void;
  sessionDiagnostics?: () => {session_id?: string; previous_session_id?: string};
  onState?: (state: SoftphoneState, detail?: string) => void;
  onNotice?: (detail:string) => void;
  onLevels?: (mic: number, speaker: number) => void;
  onDiagnostics?: (diagnostics: SoftphoneDiagnostics) => void;
  /** Pushed call progress; hosts may keep polling as a fallback. */
  onCallStatus?: (status: SoftphoneCallStatus) => void;
}

export interface SoftphoneAudioOptions {
	/** Optional browser transport; existing PCM WebSocket remains the default. */
	mediaTransport?: "websocket" | "webrtc" | "auto";
  /** WebRTC Opus redundancy. Default true when supported; false uses the portable codec. Applied on start/reconnect. */
  webrtcFec?: boolean;
  inputDeviceId?: string;
  outputDeviceId?: string;
  outputVolume?: number;
  echoCancellation: boolean;
  noiseSuppression: boolean;
  autoGainControl: boolean;
  inputGainDB: number;
  /** Initial playback cushion in ms (40–280). Default 60. Applied on start/reconnect. */
  playbackTargetMs?: number;
  /** Adaptive lower bound (40–280). Default min(60, target). */
  playbackMinMs?: number;
  /** Adaptive upper bound (40–280). Default 280. */
  playbackMaxMs?: number;
  /** Build/drain the PCM reserve with bounded waveform-matched adjustments. Default true. */
  playbackAdaptive?: boolean;
  highpassFilter: boolean;
}

export interface AudioDropEvent {
  packet_count?: number;
  ssrc?: number;
  window_ms?: number;
  timestamp: string;
  direction: string;
  reason: string;
  duration_ms: number;
  queue_before_ms?: number;
  queue_after_ms?: number;
  sequence?: number | null;
}

/** Missing caller playback samples, separate from received audio discarded. */
export interface PlaybackUnderrunEvent {
  id: string;
  started_at: string;
  ended_at?: string;
  observed_until: string;
  duration_ms: number;
  missing_samples: number;
  sample_rate: number;
  start_audio_ms: number;
  end_audio_ms: number;
  last_sequence?: number | null;
  resume_sequence?: number | null;
  end_reason: string;
  complete: boolean;
  timestamp_basis: "browser_wall_audio_clock";
  connection_id?: string;
}

export function mergePlaybackUnderruns(previous: PlaybackUnderrunEvent[], incoming: PlaybackUnderrunEvent[]): PlaybackUnderrunEvent[] {
  const events = new Map(previous.map(event => [event.id, event]));
  for (const event of incoming) {
    const prior = events.get(event.id);
    if (!prior || (!prior.complete || event.complete) && (!prior.ended_at || !!event.ended_at) && event.duration_ms >= prior.duration_ms) events.set(event.id, {...event});
  }
  return [...events.values()].sort((a, b) => a.started_at.localeCompare(b.started_at)).slice(-100);
}

// The renderer may stop before delivering its last snapshot. Preserve the
// measured interval and mark it incomplete rather than inventing missing time.
export function endPlaybackObservation(events: PlaybackUnderrunEvent[], reason: string): PlaybackUnderrunEvent[] {
  return events.map(event => event.ended_at ? event : {...event, ended_at:event.observed_until, end_reason:reason, complete:false});
}

export interface AudioObservationEvent {
  id:string; timestamp:string; connection_id?:string; kind:string; reason:string;
  phase?:string; audio_time_ms?:number; queue_ms:number; target_ms?:number;
  minimum_ms?:number; wait_ms?:number; duration_ms?:number; match_score?:number;
  queue_bytes?:number; frame_age_ms?:number; worker_delay_ms?:number;
  clock_uncertainty_ms?:number; sequence?:number;
}
export interface SoftphoneDiagnostics {
  transportSamples?: TransportSample[];
  playbackEvents?: AudioObservationEvent[];
  captureQueueEvents?: AudioObservationEvent[];
  /** Playback starvation, separate from discarded received frames. */
  underrunDurationMs?: number;
  underrunEvents?: PlaybackUnderrunEvent[];
	mediaTransport?: "websocket" | "webrtc";
	codec?: "pcm16" | "opus";
	webrtc?: { protocol?: string; candidateType?: string; sendBitrateBps?: number; receiveBitrateBps?: number; packetsLost?: number; jitterMs?: number; concealedMs?: number; packetsDiscarded?: number; jitterBufferMs?: number };
  audioHealth?: SoftphoneAudioHealth;
  sessionEvents?: MediaSessionEvent[];
  coachingPlayedMs?: number;
  coachingDroppedMs?: number;
  coachingMaxQueueMs?: number;
  rttMs: number | null;
  queueMs: number;
  targetMs: number;
  /** Native WebRTC does not expose our PCM underrun counter. */
  underruns: number | null;
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
  inputGainDB: 0,
  highpassFilter: true,
};

export function playbackBufferOptions(options: Partial<SoftphoneAudioOptions>) {
	if (options.mediaTransport !== undefined && !["websocket", "webrtc", "auto"].includes(options.mediaTransport)) throw new RangeError("Unsupported softphone media transport");
  if (options.webrtcFec !== undefined && typeof options.webrtcFec !== "boolean") throw new RangeError("webrtcFec must be boolean");
  const initialTargetMs = options.playbackTargetMs ?? JITTER_TARGET_MS;
  const minTargetMs = options.playbackMinMs ?? Math.min(JITTER_TARGET_MS, initialTargetMs);
  const maxTargetMs = options.playbackMaxMs ?? 280;
  for (const value of [initialTargetMs, minTargetMs, maxTargetMs]) {
    if (!Number.isFinite(value) || value < 40 || value > 280) throw new RangeError("Playback buffers must be between 40 and 280 ms");
  }
  if (minTargetMs > initialTargetMs || initialTargetMs > maxTargetMs) throw new RangeError("Playback buffers require min <= target <= max");
  if (options.playbackAdaptive !== undefined && typeof options.playbackAdaptive !== "boolean") throw new RangeError("playbackAdaptive must be boolean");
  return { initialTargetMs, minTargetMs, maxTargetMs, hardMaxMs: 320, adaptiveReserve:options.playbackAdaptive !== false };
}

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

  constructor(private readonly onLevel?: (level: number) => void, private readonly recordAudio = true) {}

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
      await loadAudioWorklet(this.ctx, workletURL);
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
        if (this.recordAudio) this.frames.push(new Int16Array(floatToPCM16(frame)));
        this.samples += frame.length;
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
  private clientEpoch = crypto.randomUUID();
  private transportTelemetry=new TransportTelemetry("websocket");
  private transportSender=new TransportTelemetrySender(sample=>{
    if(!this.mediaSocketConnected||!this.worker||this.diagnostics.websocketBufferedBytes>1920)return false;
    this.worker.postMessage({type:"send.telemetry",data:JSON.stringify({type:"transport.samples",diagnostics:{client_epoch:this.clientEpoch,transport_samples:[sample]}})});return true;
  },report=>{
    if(!this.mediaSocketConnected||!this.worker||this.diagnostics.websocketBufferedBytes>1920)return false;
    this.worker.postMessage({type:"send.telemetry",data:JSON.stringify(report)});return true;
  });
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
  private telemetryTimer: ReturnType<typeof setInterval> | null = null;
  private runtimeTelemetry = new AudioRuntimeTelemetry(event => this.recordSessionEvent(event));
  private opened = false;
  private cancelWorkerStart?: () => void;
  private microphoneTransportReady = false;
  private playbackHeld = false;
  private mediaSocketConnected = false;
  private ringback: (() => void) | null = null;
  private transportTiming: Record<string, unknown> = {};
  private playbackTiming: Record<string, unknown> = {};
  private workerDropEvents: AudioDropEvent[] = [];
  private playbackDropEvents: AudioDropEvent[] = [];
  private diagnostics: SoftphoneDiagnostics = {
    mediaTransport: "websocket", codec: "pcm16",
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
    const playbackOptions = playbackBufferOptions(options);
    this.diagnostics.targetMs = playbackOptions.initialTargetMs;
    this.callbacks.onState?.("connecting");
    this.runtimeTelemetry.observeEnvironment();
    this.runtimeTelemetry.tick();
    this.telemetryTimer = setInterval(() => this.runtimeTelemetry.tick(), 1000);
    try {
      this.stream = await navigator.mediaDevices.getUserMedia({ audio: microphoneConstraints(options) });
      this.ensureOpen();
      const track = this.stream.getAudioTracks()[0];
      if (!track) throw new Error("No microphone audio track was returned.");
      if (track.readyState === "ended") throw new Error("Microphone disconnected before audio setup.");
      track.onmute = () => { this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_muted"}); this.callbacks.onNotice?.("Microphone input was interrupted by the device or browser."); };
      track.onunmute = () => { this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:"device_unmuted"}); this.callbacks.onNotice?.("Microphone input restored."); };
      track.onended = () => { if (!this.closed) this.fail("Microphone disconnected. Select a microphone and reconnect audio."); };
      const applied = appliedMicrophoneSettings(track);
      this.diagnostics = {
        ...this.diagnostics, microphoneSampleRate: applied.sampleRate ?? 0,
        microphoneChannelCount: applied.channelCount ?? 0, echoCancellation: applied.echoCancellation,
        noiseSuppression: applied.noiseSuppression, autoGainControl: applied.autoGainControl,
        micInputGainDb: options.inputGainDB,
      };
      try { this.ctx = new AudioContext({ sampleRate:SAMPLE_RATE, latencyHint:"interactive" }); }
      catch { this.ctx = new AudioContext({latencyHint:"interactive"}); }
      this.runtimeTelemetry.context(this.ctx.state);
      if (this.ctx.state === "suspended") await this.ctx.resume();
      this.runtimeTelemetry.context(this.ctx.state);
      this.ensureOpen();
      if (options.outputDeviceId && "setSinkId" in this.ctx) { await (this.ctx as AudioContext & {setSinkId(id:string):Promise<void>}).setSinkId(options.outputDeviceId); this.ensureOpen(); }
      await loadAudioWorklet(this.ctx, workletURL);
      this.ensureOpen();
      this.diagnostics.audioContextRate = this.ctx.sampleRate;
      const source = this.ctx.createMediaStreamSource(this.stream);
      this.capture = new AudioWorkletNode(this.ctx, "softphone-capture", {
        processorOptions: { inputGainDB: options.inputGainDB, highpassFilter: options.highpassFilter },
      });
      this.playback = new AudioWorkletNode(this.ctx, "softphone-playback", {
        numberOfInputs: 0, outputChannelCount: [1],
        processorOptions: {...playbackOptions, telemetryEpoch:this.clientEpoch, telemetryEnabled:false},
      });
      // A headless host can reconnect an already-muted call. Apply the gate
      // before capture starts, rather than after the socket has connected.
      this.capture.port.postMessage({ type: "muted", value: this.muted });
      this.installWorkletDiagnostics();
      this.sink = this.ctx.createGain();
      this.sink.gain.value = 0;
      this.capture.connect(this.sink).connect(this.ctx.destination);
      source.connect(this.capture);
      this.output = this.ctx.createGain(); this.output.gain.value = options.outputVolume ?? 1;
      this.playback.connect(this.output).connect(this.ctx.destination);
      await this.openWorker(mediaURL, workerURL);
      this.ensureOpen();
      this.capture.onprocessorerror = this.playback.onprocessorerror = () => this.fail("Audio processing stopped. Reconnect audio.");
      this.runtimeTelemetry.context(this.ctx.state);
      this.ctx.onstatechange = () => {
        if (this.closed) return;
        this.runtimeTelemetry.context(this.ctx?.state ?? "closed");
        this.worker?.postMessage({type:"clock.reset",paused:this.ctx?.state !== "running"});
        this.setPlaybackObservation(this.ctx?.state === "running" && this.microphoneTransportReady && !this.playbackHeld, "audio_context_paused");
        if (this.ctx?.state === "suspended" || (this.ctx?.state as string) === "interrupted") this.callbacks.onState?.("reconnecting", "Browser paused audio. Reconnect audio to continue.");
        else if (this.ctx?.state === "running" && this.microphoneTransportReady) this.callbacks.onState?.("live");
      };
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

  /** Locally synthesized ringback through this session's context, so it follows the chosen output device. */
  startRingback(country?: string): void {
    if (this.closed || !this.ctx || this.ringback) return;
    this.ringback = playRingback(this.ctx, this.ctx.destination, ringbackPattern(country));
  }
  stopRingback(): void { this.ringback?.(); this.ringback = null; }

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
      if (stats?.type === "playback.underrun") {
        this.diagnostics.underrunEvents = mergePlaybackUnderruns(this.diagnostics.underrunEvents ?? [], [stats.event]);
        this.diagnostics.underrunDurationMs = stats.underrun_ms;
        this.diagnostics.underruns = stats.underruns;
        return;
      }
      if (stats?.type !== "stats") return;
      this.diagnostics.playbackEvents=stats.playback_events;
      this.playbackTiming = {reserve_expanded_ms:stats.reserve_expanded_ms,reserve_compressed_ms:stats.reserve_compressed_ms,reserve_adjustments:stats.reserve_adjustments,reserve_match_rejections:stats.reserve_match_rejections,played_ms:stats.played_ms,max_residence_ms:stats.max_residence_ms,drop_totals_ms:stats.drop_totals_ms,coaching:{played_ms:stats.whisper_played_ms,dropped_ms:stats.whisper_dropped_ms,max_queue_ms:stats.whisper_max_queue_ms}};
      this.speakerLevel = Math.max(this.speakerLevel, stats.speaker_level ?? 0);
      this.diagnostics = {
        ...this.diagnostics, coachingPlayedMs:stats.whisper_played_ms ?? 0, coachingDroppedMs:stats.whisper_dropped_ms ?? 0, coachingMaxQueueMs:stats.whisper_max_queue_ms ?? 0, queueMs: stats.queue_ms ?? 0, targetMs: stats.target_ms ?? JITTER_TARGET_MS,
        underruns: stats.underruns ?? 0, droppedMs: stats.dropped_ms ?? 0,
        underrunDurationMs: stats.underrun_ms ?? 0,
        underrunEvents: mergePlaybackUnderruns(this.diagnostics.underrunEvents ?? [], stats.underrun_events ?? []),
        maxQueueMs: stats.max_queue_ms ?? 0, playbackSequenceGaps: stats.playback_sequence_gaps ?? 0,
        dropEvents: this.mergeDropEvents(undefined, stats.drop_events ?? []),
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
      this.cancelWorkerStart = () => finish(new Error("audio session closed"));
      worker.onmessage = (event: MessageEvent) => {
        if (this.closed) { finish(new Error("audio session closed")); return; }
        const message = event.data;
        if (message?.type === "socket.open") {
          this.reportedPlaybackIDs.clear();this.reportedCaptureQueueIDs.clear();
          this.opened = true;
          this.mediaSocketConnected = true;
          this.startRTTProbe();
          finish();
        } else if (message?.type === "runtime.event") {
          this.recordSessionEvent(message.event);
        } else if (message?.type === "socket.message") {
          this.handleControl(message.data);
        } else if (message?.type === "socket.reconnect") {
          if(this.callbacks.refreshMediaURL) void this.callbacks.refreshMediaURL(message.recovery).then(mediaURL=>{
            if(!this.closed && this.worker===worker) worker.postMessage({type:"socket.credentials",id:message.id,mediaURL});
          },error=>{
            if(this.closed || this.worker!==worker) return;
            const failure=mediaFailure(error);
            this.recordSessionEvent({timestamp:new Date().toISOString(),action:"reconnect",outcome:failure.denied ? "revoked" : "retrying",...message.recovery,status:failure.status,code:failure.code});
            worker.postMessage({type:"socket.credentials",id:message.id,denied:failure.denied});
          });
        } else if (message?.type === "socket.error") {
          this.recordSessionEvent({timestamp:message.timestamp,action:"websocket",outcome:"transport_error"});
        } else if (message?.type === "socket.close") {
          this.recordSessionEvent({timestamp:message.timestamp,action:"websocket",outcome:"closed",code:String(message.code),detail:message.reason,was_clean:message.wasClean});
          this.stopRTTProbe();
          this.mediaSocketConnected = false;
          this.microphoneTransportReady = false;
          this.setPlaybackObservation(false, "transport_disconnected");
          if (this.opened && !this.closed) this.callbacks.onState?.("reconnecting", "Connection interrupted; retrying…");
          else finish(new Error("audio connection closed before it was ready"));
        } else if (message?.type === "socket.failed") {
          this.mediaSocketConnected = false;
          finish(new Error(message.detail || "audio connection lost"));
          this.fail(message.detail || "audio connection lost");
        } else if (message?.type === "transport.drop" && message.event) {
          this.diagnostics.dropEvents = this.mergeDropEvents(message.event);
        } else if (message?.type === "transport.stats") {
          this.diagnostics.websocketBufferedBytes = message.buffered_bytes ?? 0;
          this.diagnostics.captureQueueEvents=message.capture_queue_events;
          this.transportTiming = message.timing ?? {};
          if(message.observation)this.transportTelemetry.observe({...this.transportTiming,...message.observation,rtt_ms:message.timing?.rtt_ms,queue_ms:this.diagnostics.queueMs,target_ms:this.diagnostics.targetMs,buffered_bytes:message.buffered_bytes,underruns:this.diagnostics.underruns,dropped_ms:this.diagnostics.droppedMs,capture_sequence_gaps:this.diagnostics.captureSequenceGaps,playback_sequence_gaps:this.diagnostics.playbackSequenceGaps,main_thread_max_pause_ms:this.runtimeTelemetry.counters.main_thread_max_pause_ms},
            {connection:this.mediaSocketConnected?"connected":"reconnecting",context:this.ctx?.state,muted:String(this.muted),device_muted:String(this.stream?.getAudioTracks()[0]?.muted),track:this.stream?.getAudioTracks()[0]?.readyState,codec:"pcm16"},performance.now(),message.observation?.timestamp,message.observation?.window_ms);
          this.diagnostics.transportSamples=this.transportTelemetry.recent();
          if (typeof message.timing?.rtt_ms === "number") this.diagnostics.rttMs = Math.round(message.timing.rtt_ms);
        }
      };
      worker.onerror = (event) => { this.recordSessionEvent({timestamp:new Date().toISOString(),action:"audio_worker",outcome:"error",detail:event.message?.slice(0,160)}); finish(new Error("audio worker failed")); if (!this.closed) this.fail("Audio worker failed. Reconnect audio."); };
      worker.postMessage({
        type: "init", refreshCredentials:Boolean(this.callbacks.refreshMediaURL), audioClockMS:(this.ctx?.currentTime ?? 0)*1000, monotonicEpochMS:performance.timeOrigin+performance.now(), mediaURL, contextRate: this.ctx?.sampleRate ?? SAMPLE_RATE, muted: this.muted,
        capturePort: captureChannel.port2, playbackPort: playbackChannel.port2,
      }, [captureChannel.port2, playbackChannel.port2]);
    });
  }

  // Worklet statistics are a rolling snapshot, while Worker drops are events.
  // Keep their bounded histories separately so neither overwrites the other.
  private mergeDropEvents(workerEvent?: AudioDropEvent, playbackEvents?: AudioDropEvent[]): AudioDropEvent[] {
    if (workerEvent) this.workerDropEvents = [...this.workerDropEvents, workerEvent].slice(-100);
    if (playbackEvents) this.playbackDropEvents = playbackEvents.slice(-100);
    return [...this.workerDropEvents, ...this.playbackDropEvents]
      .sort((a, b) => a.timestamp.localeCompare(b.timestamp)).slice(-100);
  }

  private carrierDeliveryStalled = false;
  private deliveryDegradedNotice = false;
  private shutdownIntent = "session_cleanup";
  private reportedPlaybackIDs=new Set<string>();
  private reportedCaptureQueueIDs=new Set<string>();

  private handleControl(data: string): void {
    if (this.closed) return;
    try {
      const parsed = JSON.parse(data) as { type?: string; detail?: string; nonce?: number; capture_sequence_gaps?:number; call_id?: string; status?: string };
      if (parsed.type === "dtmf.error" || parsed.type === "dtmf.sent") { this.callbacks.onNotice?.(parsed.type === "dtmf.sent" ? "Keypad tone sent" : parsed.detail || "Keypad tone failed");
      } else if (parsed.type === "pong") {
        this.diagnostics.captureSequenceGaps = parsed.capture_sequence_gaps ?? this.diagnostics.captureSequenceGaps;
        if (typeof parsed.nonce === "number" && parsed.nonce >= 0) this.diagnostics.rttMs = Math.max(0, Math.round(performance.now() - parsed.nonce));
        this.callbacks.onDiagnostics?.({ ...this.diagnostics });
      } else if (parsed.type === "call.ended" || parsed.type === "session.replaced") {
        this.setPlaybackObservation(false, parsed.type === "call.ended" ? "call_ended" : "observation_ended");
        this.shutdownIntent=parsed.type === "call.ended" ? "call_ended" : "session_replaced";
        this.closed = true;
        try { this.callbacks.onState?.("ended", parsed.type); }
        finally { this.teardown(); }
      } else if (parsed.type === "call.status" && typeof parsed.call_id === "string" && typeof parsed.status === "string") {
        if ((parsed as SoftphoneCallStatus).hold_state) {
          this.playbackHeld = (parsed as SoftphoneCallStatus).hold_state !== "active";
          this.setPlaybackObservation(!this.playbackHeld && this.microphoneTransportReady && this.ctx?.state === "running", this.playbackHeld ? "hold" : "observation_resumed");
        }
        if ((parsed as SoftphoneCallStatus).hold_state && (parsed as SoftphoneCallStatus).hold_state !== "active") {
          this.worker?.postMessage({ type: "flush" });
        }
        this.callbacks.onCallStatus?.(parsed as unknown as SoftphoneCallStatus);
      } else if (parsed.type === "call.error") {
        this.recordSessionEvent({timestamp:new Date().toISOString(),action:"carrier",outcome:"audio_error",detail:parsed.detail?.slice(0,160)});
        this.fail(parsed.detail || "The call could not be connected.");
      } else if (parsed.type === "coach.state") {
        this.callbacks.onNotice?.((parsed as unknown as {talking?:boolean}).talking ? "Private coaching connected. Only you hear the supervisor." : "Private coaching stopped.");
      } else if (parsed.type === "audio.health") {
        const health = parsed as unknown as SoftphoneAudioHealth;
        if (health.state !== "healthy" && health.state !== "audio_degraded") return;
        this.diagnostics.audioHealth = {state:health.state, reason:health.reason, stages:health.stages};
        this.callbacks.onAudioHealth?.(this.diagnostics.audioHealth);
        this.callbacks.onDiagnostics?.({...this.diagnostics});
        if (health.state === "audio_degraded" && !this.deliveryDegradedNotice) {
          this.deliveryDegradedNotice = true;
          this.callbacks.onNotice?.("Speech delivery is interrupted. The call remains connected.");
        } else if (this.deliveryDegradedNotice && health.state === "healthy" && health.stages?.telephony_to_browser?.state === "healthy" && this.ctx?.state === "running") {
          this.deliveryDegradedNotice = false;
          this.callbacks.onNotice?.("Speech delivery restored.");
        }
      } else if (parsed.type === "media.delivery") {
        const state = (parsed as unknown as {state?:string}).state;
        if (state === "stalled") this.callbacks.onNotice?.("Caller audio delivery interrupted. Your microphone remains connected.");
        else if (state === "flowing" && this.carrierDeliveryStalled) this.callbacks.onNotice?.("Caller audio delivery restored.");
        this.carrierDeliveryStalled = state === "stalled";
      } else if (parsed.type === "peer.disconnected") {
        this.microphoneTransportReady = false;
        this.setPlaybackObservation(false, "carrier_disconnected");
        this.worker?.postMessage({ type: "microphone.ready", value: false });
        this.worker?.postMessage({ type: "flush" });
        this.callbacks.onState?.("reconnecting", "Carrier audio interrupted; reconnecting…");
      } else if (parsed.type === "peer.connected") {
        this.microphoneTransportReady = true;
        this.setPlaybackObservation(this.ctx?.state === "running" && !this.playbackHeld, "carrier_connected");
        this.worker?.postMessage({ type: "microphone.ready", value: true });
        this.callbacks.onState?.("live", this.opened ? undefined : "Audio reconnected");
      }
    } catch { /* Status frames are advisory. */ }
  }

  private startRTTProbe(): void {
    this.stopRTTProbe();
    const ping = () => {
      // RTT is measured in the Worker, independent of main-thread delays.
      this.sendDiagnostics();
    };
    ping();
    this.pingTimer = setInterval(ping, 5_000);
  }

  private sendText(data: string): void { this.worker?.postMessage({ type: "send.text", data }); }

  recordSessionEvent(event: MediaSessionEvent): void {
    try{event={...this.callbacks.sessionDiagnostics?.(),...event};}catch{/* observer isolation */}
    event=safeMediaSessionEvent(event);
    this.diagnostics.sessionEvents=[...(this.diagnostics.sessionEvents ?? []),event].slice(-50);
    try { this.callbacks.onSessionEvent?.(event); } catch { /* host observer isolation */ }
    // Sparse events share the existing paced, bounded telemetry sender. Never
    // push a full diagnostics history synchronously for a recovery callback.
    try {
      const report={type:"diagnostics.events",diagnostics:{client_epoch:this.clientEpoch,session_events:[event]}};
      if(new TextEncoder().encode(JSON.stringify(report)).byteLength<=1100)this.transportSender.enqueueAuxiliary(report);
      else this.transportSender.skippedAuxiliary++;
    } catch { /* diagnostics cannot interrupt media recovery */ }
  }

  private sendDiagnostics(): void {
    const value = this.diagnostics;
    const fresh=(events:AudioObservationEvent[]|undefined,seen:Set<string>)=>{
      const bounded=(events??[]).slice(-64),pending=bounded.filter(e=>!seen.has(e.id)).slice(0,8);
      if(this.mediaSocketConnected){
        const retained=new Set(bounded.map(e=>e.id));for(const id of seen)if(!retained.has(id))seen.delete(id);
        for(const e of pending)seen.add(e.id);
      }
      return pending;
    };
    const playbackEvents=fresh(value.playbackEvents,this.reportedPlaybackIDs);
    const captureQueueEvents=fresh(value.captureQueueEvents,this.reportedCaptureQueueIDs);
    this.sendText(JSON.stringify({ type: "diagnostics", diagnostics: {
      media_transport: "websocket", codec: "pcm16",
      client_epoch:this.clientEpoch,
      playback_events:playbackEvents, capture_queue_events:captureQueueEvents,
      timing: {transport:this.transportTiming, playback:this.playbackTiming, runtime:this.runtimeTelemetry.counters},
      connection_state: this.mediaSocketConnected ? "connected" : this.closed ? "closed" : "reconnecting",
      carrier_peer_connected: this.microphoneTransportReady,
      audio_context_state: this.ctx?.state ?? "closed",
      microphone_muted: this.muted,
      microphone_track_state: this.stream?.getAudioTracks()[0]?.readyState ?? "ended",
      microphone_device_muted: this.stream?.getAudioTracks()[0]?.muted ?? false,
      rtt_ms: value.rttMs, playback_queue_ms: value.queueMs, playback_target_ms: value.targetMs,
      playback_max_queue_ms: value.maxQueueMs, playback_underruns: value.underruns,
      playback_underrun_ms: value.underrunDurationMs, playback_underrun_events:value.underrunEvents,
      playback_dropped_ms: value.droppedMs, websocket_buffered_bytes: value.websocketBufferedBytes,
      audio_context_rate: value.audioContextRate, microphone_sample_rate: value.microphoneSampleRate,
      microphone_channel_count: value.microphoneChannelCount, echo_cancellation: value.echoCancellation,
      noise_suppression: value.noiseSuppression, auto_gain_control: value.autoGainControl,
      mic_active_rms_dbfs: value.micActiveRmsDbfs, mic_peak_dbfs: value.micPeakDbfs,
      mic_post_peak_dbfs: value.micPostPeakDbfs, mic_input_gain_db: value.micInputGainDb,
      mic_limiter_reduction_db: value.micLimiterReductionDb, capture_sequence_gaps: value.captureSequenceGaps,
      playback_sequence_gaps: value.playbackSequenceGaps, drop_events: value.dropEvents,
    }}));
    if(this.mediaSocketConnected)this.transportSender.enqueue(this.transportTelemetry.drain());
    this.callbacks.onDiagnostics?.({ ...value });
  }

  private stopRTTProbe(): void {
    if (this.pingTimer !== null) clearInterval(this.pingTimer);
    this.pingTimer = null;
  }

  private setPlaybackObservation(active: boolean, reason: string): void {
    try {
      this.playback?.port.postMessage({type:"playback.telemetry.boundary",active,reason,audio_time_ms:(this.ctx?.currentTime ?? 0)*1000});
      if (!active) this.diagnostics.underrunEvents = endPlaybackObservation(this.diagnostics.underrunEvents ?? [], reason);
    } catch { /* observation cannot affect audio or recovery */ }
  }

  setMuted(muted: boolean): void {
    const changed = this.muted !== muted;
    this.muted = muted;
    this.worker?.postMessage({type:"muted",value:muted});
    this.capture?.port.postMessage({ type: "muted", value: muted });
    if (muted) this.sendText(JSON.stringify({ type: "interrupt" }));
    if (changed) this.recordSessionEvent({timestamp:new Date().toISOString(),action:"microphone",outcome:muted ? "muted" : "unmuted"});
  }

  stop(): void {
    this.shutdownIntent="user_stop";
    const notify = !this.closed;
    this.closed = true;
    try { if (notify) this.callbacks.onState?.("ended"); }
    finally { this.teardown(); }
  }

  private fail(detail: string): void {
    this.shutdownIntent="audio_error";
    if (!this.closed) this.recordSessionEvent({timestamp:new Date().toISOString(),action:"audio",outcome:"error",detail:detail.slice(0,160)});
    this.closed = true;
    try { this.callbacks.onState?.("error", detail); }
    finally { this.teardown(); }
  }

  private teardown(): void {
    this.runtimeTelemetry.stopEnvironment();
    this.transportSender.stop();
    this.setPlaybackObservation(false, "observation_ended");
    this.stopRingback();
    this.microphoneTransportReady = false;
    this.cancelWorkerStart?.();
    this.cancelWorkerStart = undefined;
    try { this.sendDiagnostics(); } catch { /* diagnostics cannot prevent device cleanup */ }
    this.stopRTTProbe();
    if (this.telemetryTimer !== null) clearInterval(this.telemetryTimer);
    this.telemetryTimer = null;
    if (this.ctx) { this.ctx.onstatechange = null; this.runtimeTelemetry.context("closed"); }
    if (this.levelTimer !== null) clearInterval(this.levelTimer);
    this.levelTimer = null;
    const worker = this.worker;
    worker?.postMessage({ type: "close", reason:this.shutdownIntent });
    if (worker) {
      const timeout=setTimeout(() => worker.terminate(),600);
      worker.onmessage=(event)=>{if(event.data?.type === "socket.shutdown.complete") {clearTimeout(timeout);worker.terminate();}};
    }
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
