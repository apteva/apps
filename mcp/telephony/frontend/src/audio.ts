import { SoftphoneSession, MicrophoneTestSession, DEFAULT_SOFTPHONE_AUDIO_OPTIONS, microphoneConstraints, type SoftphoneAudioOptions, type SoftphoneCallbacks } from "../../ui/softphone-audio";
import workletSource from "../../ui/softphone-worklet.js" with { type: "text" };
import workerSource from "../../ui/softphone-worker.js" with { type: "text" };

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
export function createMicrophonePreview(onLevel?: (level: number) => void): MicrophonePreview {
  const session = new MicrophoneTestSession(onLevel, false);
  let url: string | undefined;
  let used = false;
  let stopping: Promise<void> | undefined;
  const stop = () => {
    used = true;
    return stopping ??= (async () => {
      try { await session.cancel(); }
      finally {
        if (url) URL.revokeObjectURL(url);
        url = undefined;
      }
    })();
  };
  return {
    async start(options = {}) {
      if (used) throw new Error("Microphone preview is already used");
      used = true;
      url = URL.createObjectURL(new Blob([workletSource], { type: "text/javascript" }));
      try { await session.start(url, { ...DEFAULT_SOFTPHONE_AUDIO_OPTIONS, ...options }); }
      catch (error) { await stop(); throw error; }
    },
    stop,
  };
}

export interface AudioConnection {
  start(url: string, options: SoftphoneAudioOptions): Promise<void>;
  stop(): void;
  setMuted(muted: boolean): void;
  sendDTMF(digits: string): void;
  setOutputVolume(volume: number): void;
}
export interface AudioRuntime {
  preflight(options: SoftphoneAudioOptions): Promise<void>;
  create(callbacks: SoftphoneCallbacks): AudioConnection;
}

/** Source strings are inside the integrity-checked client bundle, not fetched separately. */
export const browserAudio: AudioRuntime = {
  async preflight(options) {
    const stream = await navigator.mediaDevices.getUserMedia({ audio: microphoneConstraints(options) });
    stream.getTracks().forEach(track => track.stop());
  },
  create(callbacks) {
    const session = new SoftphoneSession(callbacks);
    const urls: string[] = [];
    const stop = () => {
      try { session.stop(); }
      finally { urls.splice(0).forEach(url => URL.revokeObjectURL(url)); }
    };
    return {
      async start(url, options) {
        try {
          for (const source of [workletSource, workerSource]) urls.push(URL.createObjectURL(new Blob([source], { type: "text/javascript" })));
          await session.start(url, urls[0], urls[1], options);
        } catch (error) { stop(); throw error; }
      },
      stop,
      setMuted: value => session.setMuted(value),
      sendDTMF: digits => session.sendDTMF(digits),
      setOutputVolume: value => session.setOutputVolume(value),
    };
  },
};
