import { measureAudioSetup } from "./audio-setup";
import type { MediaSessionEvent } from "../frontend/src/media-lease";

export interface PreparedAudioDevices { stream: MediaStream; context: AudioContext }
export const AUDIO_PREFLIGHT_TIMEOUT_MS = 10000;

/** Own devices until one transport takes them. Start playback permission in
 * the Answer click, before awaiting HTTP, and never reopen a tested microphone. */
export class AudioPreparation {
  readonly ready: Promise<void>;
  private context?: AudioContext;
  private stream?: MediaStream;
  private cancelled = false;
  private completed = false;
  private cancelWait?: () => void;

  constructor(constraints: MediaTrackConstraints, contextOptions: AudioContextOptions,
              emit?: (event: MediaSessionEvent) => void) {
    this.ready = this.prepare(constraints, contextOptions, emit);
  }

  private async prepare(constraints: MediaTrackConstraints, options: AudioContextOptions,
                        emit?: (event: MediaSessionEvent) => void): Promise<void> {
    let timer: ReturnType<typeof setTimeout> | undefined;
    try {
      try { this.context = new AudioContext(options); }
      catch { this.context = new AudioContext({latencyHint:"interactive"}); }
      const context = this.context;
      // Calling resume now (not after getUserMedia or fetch) preserves the
      // browser's transient user activation for playback.
      const playback = measureAudioSetup("audio_context_preflight", emit, () => context.resume());
      const microphone = measureAudioSetup("microphone_preflight", emit, async () => {
        const stream = await navigator.mediaDevices.getUserMedia({audio:constraints});
        if (this.cancelled) { stream.getTracks().forEach(track => track.stop()); throw Error("Audio preparation cancelled"); }
        this.stream = stream;
        if (!stream.getAudioTracks().some(track => track.readyState === "live")) throw Error("Microphone is unavailable");
      });
      await Promise.race([
        Promise.all([playback, microphone]),
        new Promise<never>((_, reject) => {
          this.cancelWait = () => reject(Error("Audio preparation cancelled"));
          timer = setTimeout(() => reject(Error("Microphone or playback setup timed out. Check browser permissions and try again.")), AUDIO_PREFLIGHT_TIMEOUT_MS);
        }),
      ]);
      if (this.cancelled || context.state !== "running") throw Error("Browser audio is paused. Click Answer again to enable audio.");
      this.completed = true;
    } catch (error) { this.cancel(); throw error; }
    finally { clearTimeout(timer); this.cancelWait = undefined; }
  }

  take(): PreparedAudioDevices {
    if (!this.completed || this.cancelled || !this.stream || !this.context) throw Error("Audio preparation is unavailable");
    if (!this.stream.getAudioTracks().some(track => track.readyState === "live")) throw Error("Microphone disconnected before audio setup. Select a microphone and try again.");
    const devices = {stream:this.stream, context:this.context};
    this.stream = undefined; this.context = undefined;
    return devices;
  }

  cancel(): void {
    this.cancelled = true;
    this.cancelWait?.();
    this.stream?.getTracks().forEach(track => track.stop()); this.stream = undefined;
    const context = this.context; this.context = undefined;
    if (context) void context.close().catch(() => {});
  }
}
