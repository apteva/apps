import type { MediaSessionEvent } from "./media-lease";
import { SoftphoneSession, MicrophoneTestSession, DEFAULT_SOFTPHONE_AUDIO_OPTIONS, microphoneConstraints, type SoftphoneAudioOptions, type SoftphoneCallbacks } from "../../ui/softphone-audio";
import type { AppHandle } from "@apteva/web-sdk";
import { embeddedAudioModules, loadAudioModules, type AudioModules } from "./audio-assets";
import { selectableAudio, WebRTCAudioConnection } from "./webrtc-audio";

export interface MicrophoneDevice { deviceId: string; label: string }
export interface MicrophonePreview {
  start(options?: Partial<SoftphoneAudioOptions>): Promise<void>;
  stop(): Promise<void>;
}

/** Enumerates inputs without opening the microphone or requesting permission. */
export async function listMicrophones(): Promise<MicrophoneDevice[]> {
  const devices = await navigator.mediaDevices.enumerateDevices();
  return devices.filter(device => device.kind === "audioinput").map((device, index) => ({
    deviceId: device.deviceId, label: device.label || `Microphone ${index + 1}`,
  }));
}

/** Local meter using the call capture pipeline; no recording, carrier, or socket. */
export function createMicrophonePreview(onLevel?: (level: number) => void, app?: AppHandle): MicrophonePreview {
  const session = new MicrophoneTestSession(onLevel, false);
  let modules: AudioModules | undefined;
  let used = false;
  let stopping: Promise<void> | undefined;
  const stop = () => {
    used = true;
    return stopping ??= (async () => {
      try { await session.cancel(); }
      finally {
        modules?.dispose();
        modules = undefined;
      }
    })();
  };
  return {
    async start(options = {}) {
      if (used) throw new Error("Microphone preview is already used");
      used = true;
      try {
        modules = app ? await loadAudioModules(app, true) : embeddedAudioModules(true);
        if (stopping) { modules.dispose(); modules = undefined; throw new Error("Microphone preview was cancelled"); }
        await session.start(modules.urls[0], { ...DEFAULT_SOFTPHONE_AUDIO_OPTIONS, ...options });
      }
      catch (error) { await stop(); throw error; }
    },
    stop,
  };
}

export interface AudioConnection {
  start(url: string, options: SoftphoneAudioOptions): Promise<void>;
  stop(): void;
  recordSessionEvent?(event: MediaSessionEvent): void;
  setMuted(muted: boolean): void;
  sendDTMF(digits: string): void;
  setOutputVolume(volume: number): void;
  /** Optional locally synthesized ringback; runtimes without audio output may omit it. */
  startRingback?(country?: string): void;
  stopRingback?(): void;
}
export interface AudioRuntime {
  preflight(options: SoftphoneAudioOptions): Promise<void>;
  create(callbacks: SoftphoneCallbacks): AudioConnection;
}

/** Source strings are inside the integrity-checked client bundle, not fetched separately. */
function createWebSocketAudio(app?: AppHandle): AudioRuntime { return {
  async preflight(options) {
    const stream = await navigator.mediaDevices.getUserMedia({ audio: microphoneConstraints(options) });
    stream.getTracks().forEach(track => track.stop());
  },
  create(callbacks) {
    const session = new SoftphoneSession(callbacks);
    let modules: AudioModules | undefined;
    let stopped = false;
    const stop = () => {
      stopped = true;
      try { session.stop(); }
      finally { modules?.dispose(); modules = undefined; }
    };
    return {
      async start(url, options) {
        try {
          modules = app ? await loadAudioModules(app) : embeddedAudioModules();
          if (stopped) throw new Error("Audio session was cancelled");
          await session.start(url, modules.urls[0], modules.urls[1], options);
        } catch (error) { stop(); throw error; }
      },
      stop,
      recordSessionEvent: event => session.recordSessionEvent(event),
      setMuted: value => session.setMuted(value),
      sendDTMF: digits => session.sendDTMF(digits),
      setOutputVolume: value => session.setOutputVolume(value),
      startRingback: country => session.startRingback(country),
      stopRingback: () => session.stopRingback(),
    };
  },
}; }

export function createBrowserAudio(app?: AppHandle): AudioRuntime {
  const websocket = createWebSocketAudio(app);
  return {
    preflight: options => websocket.preflight(options),
    create(callbacks) {
      return selectableAudio(callbacks, () => websocket.create(callbacks), () => {
        const connection = new WebRTCAudioConnection(callbacks);
        let modules: AudioModules | undefined, stopped = false;
        return {
          async start(url, options) {
            modules = app ? await loadAudioModules(app) : embeddedAudioModules();
            if (stopped) { modules.dispose(); modules=undefined; throw new Error("Audio session cancelled"); }
            await connection.start(url, options, modules.urls[0]);
          },
          stop() { stopped=true; connection.stop(); modules?.dispose(); modules=undefined; },
          setMuted:value=>connection.setMuted(value), sendDTMF:digits=>connection.sendDTMF(digits),
          setOutputVolume:value=>connection.setOutputVolume(value), recordSessionEvent:event=>connection.recordSessionEvent(event),
          startRingback:country=>connection.startRingback(country), stopRingback:()=>connection.stopRingback(),
        };
      });
    },
  };
}
