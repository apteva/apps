import { SoftphoneSession, microphoneConstraints, type SoftphoneAudioOptions, type SoftphoneCallbacks } from "../../ui/softphone-audio";
import workletSource from "../../ui/softphone-worklet.js" with { type: "text" };
import workerSource from "../../ui/softphone-worker.js" with { type: "text" };

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
